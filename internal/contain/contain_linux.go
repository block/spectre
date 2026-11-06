//go:build linux

package contain

import (
	"github.com/alecthomas/errors"
	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// install replaces the table with one whose rules reject uid's IPv4 and IPv6
// traffic, in one atomic batch.
func install(uid uint32) error {
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
	chain := conn.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &accept,
	})
	for _, exprs := range rules(uid) {
		conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs})
	}
	return errors.Wrap(conn.Flush(), "install packet filter rules")
}

// rules accept uid's packets on established connections, such as replies to
// connections opened to it, and reject the rest as if nothing were listening.
func rules(uid uint32) [][]expr.Any {
	owner := func() []expr.Any {
		return []expr.Any{
			&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uid)},
		}
	}
	established := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	none := binaryutil.NativeEndian.PutUint32(0)
	return [][]expr.Any{
		append(owner(),
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: established, Xor: none},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: none},
			&expr.Verdict{Kind: expr.VerdictAccept},
		),
		append(owner(), &expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_PORT_UNREACH}),
	}
}
