//go:build linux

package contain

import (
	"github.com/alecthomas/errors"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// destinationNAT is the connection tracking status bit of connections whose
// destination was rewritten, such as by a redirect.
const destinationNAT uint32 = 1 << 5

// install replaces the table with one whose rules redirect the configured
// connections and reject the user's other IPv4 and IPv6 traffic, in one atomic batch.
func install(config Config) error {
	conn, err := nftables.New()
	if err != nil {
		return errors.Wrap(err, "open packet filter connection")
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	// Adding before deleting removes a previous run's table, and succeeds when there is none.
	conn.AddTable(table)
	conn.DelTable(table)
	conn.AddTable(table)
	accept := nftables.ChainPolicyAccept
	if len(config.Redirects) > 0 {
		// Running before the other destination NAT chains, such as a service mesh's,
		// means a redirected connection is rewritten by no other rule.
		redirect := conn.AddChain(&nftables.Chain{
			Name:     "redirect",
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookOutput,
			Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityNATDest - 1),
			Policy:   &accept,
		})
		for _, exprs := range redirectRules(config.Redirects) {
			conn.AddRule(&nftables.Rule{Table: table, Chain: redirect, Exprs: exprs})
		}
	}
	output := conn.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &accept,
	})
	for _, exprs := range rules(config) {
		conn.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: exprs})
	}
	return errors.Wrap(conn.Flush(), "install packet filter rules")
}

// redirectRules send each redirect's TCP connections to its loopback port.
func redirectRules(redirects []Redirect) [][]expr.Any {
	rules := make([][]expr.Any, 0, len(redirects))
	for _, redirect := range redirects {
		rules = append(rules, append(tcpOf(redirect.UID, redirect.Port),
			&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(redirect.LocalPort)},
			&expr.Redir{RegisterProtoMin: 1, RegisterProtoMax: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
		))
	}
	return rules
}

// rules accept the user's packets on established connections, such as replies to
// connections opened to it, and on its redirected connections, and reject the rest
// as if nothing were listening.
func rules(config Config) [][]expr.Any {
	established := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	none := binaryutil.NativeEndian.PutUint32(0)
	accepted := [][]expr.Any{
		append(owner(config.UID),
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: established, Xor: none},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: none},
			&expr.Verdict{Kind: expr.VerdictAccept},
		),
	}
	for _, redirect := range config.Redirects {
		if redirect.UID != config.UID {
			continue
		}
		// Only connections that a redirect rewrote pass, so the user reaches no other local port.
		accepted = append(accepted, append(tcpOf(config.UID, redirect.LocalPort),
			&expr.Ct{Register: 1, Key: expr.CtKeySTATUS},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(destinationNAT), Xor: none},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: none},
			&expr.Verdict{Kind: expr.VerdictAccept},
		))
	}
	return append(accepted,
		append(owner(config.UID), &expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_PORT_UNREACH}))
}

// owner matches the packets of uid's sockets.
func owner(uid uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uid)},
	}
}

// tcpOf matches uid's TCP packets to port.
func tcpOf(uid uint32, port uint16) []expr.Any {
	return append(owner(uid),
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(port)},
	)
}
