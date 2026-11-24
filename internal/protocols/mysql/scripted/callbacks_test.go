package scripted_test

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
)

// callbacks runs function with the first statement replacement that scripts declared, and the
// callbacks of a fresh evaluator.
func callbacks(t *testing.T, loaded *comparison.Scripts, handle scripted.Handle, function func(replacement scripted.Replacement, callbacks scripted.Callbacks) error) error {
	t.Helper()
	replacements := handle.Declared(loaded.Program()).Replacements
	assert.NotZero(t, len(replacements))
	return loaded.Evaluate(t.Context(), func(evaluator *javascript.Evaluator) error {
		return function(replacements[0], handle.Instance(evaluator))
	})
}

// The candidate joins orders to items where the reference read the orders, then each
// order's items: the function claims each reference statement and joins their rows.
func TestCombiningClaimsEachSource(t *testing.T) {
	loaded, handle := scripts(t, combineScript)
	claimed := [][]Option[string]{}
	orders := scripted.Claim(func(values []Option[string]) ([]scripted.Row, error) {
		claimed = append(claimed, values)
		return []scripted.Row{{"id": Some("7"), "label": Some("first")}, {"id": Some("8"), "label": None[string]()}}, nil
	})
	items := scripted.Claim(func(values []Option[string]) ([]scripted.Row, error) {
		claimed = append(claimed, values)
		if values[0] == Some("8") {
			return []scripted.Row{}, nil
		}
		return []scripted.Row{{"id": Some("1"), "sku": Some("apple")}}, nil
	})

	var rows []scripted.Row
	err := callbacks(t, loaded, handle, func(replacement scripted.Replacement, callbacks scripted.Callbacks) error {
		var err error
		rows, err = callbacks.Combine(replacement, []Option[string]{Some("1")}, []scripted.Claim{orders, items})
		return err
	})
	assert.NoError(t, err)

	assert.Equal(t, [][]Option[string]{{Some("1")}, {Some("7")}, {Some("8")}}, claimed)
	assert.Equal(t, []scripted.Row{
		{"id": Some("7"), "label": Some("first"), "item": Some("1"), "sku": Some("apple")},
		{"id": Some("8"), "label": None[string](), "item": None[string](), "sku": None[string]()},
	}, rows)
}

// The reference joins orders to items where the candidate reads the orders, then each
// order's items: the function splits the JOIN into one reply per candidate statement.
func TestSplittingOffersEachReply(t *testing.T) {
	loaded, handle := scripts(t, splitScript)
	type offer struct {
		index  int
		values []Option[string]
		rows   []scripted.Row
	}
	offers := []offer{}

	err := callbacks(t, loaded, handle, func(replacement scripted.Replacement, callbacks scripted.Callbacks) error {
		return callbacks.Split(replacement, []Option[string]{Some("1")}, []scripted.Row{
			{"id": Some("7"), "label": Some("first"), "item": Some("1"), "sku": Some("apple")},
			{"id": Some("7"), "label": Some("first"), "item": Some("2"), "sku": None[string]()},
			{"id": Some("8"), "label": Some("second"), "item": None[string](), "sku": None[string]()},
		}, func(index int, values []Option[string], rows []scripted.Row) error {
			offers = append(offers, offer{index, values, rows})
			return nil
		})
	})
	assert.NoError(t, err)

	assert.Equal(t, []offer{
		{0, []Option[string]{Some("1")}, []scripted.Row{{"id": Some("7"), "label": Some("first")}, {"id": Some("8"), "label": Some("second")}}},
		{1, []Option[string]{Some("7")}, []scripted.Row{{"id": Some("1"), "sku": Some("apple")}, {"id": Some("2"), "sku": None[string]()}}},
		{1, []Option[string]{Some("8")}, []scripted.Row{}},
	}, offers)
}

// The reference never ran a statement that the script claims, and the script catches the
// failure; the candidate still cannot be answered with what the script returns.
func TestClaimFailuresEndTheCombinationEvenWhenCaught(t *testing.T) {
	unmatched := errors.New("no reference reply")
	loaded, handle := scripts(t, `egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(
		([value], [claim]) => { try { claim(value); } catch { } return []; });`)

	err := callbacks(t, loaded, handle, func(replacement scripted.Replacement, callbacks scripted.Callbacks) error {
		_, err := callbacks.Combine(replacement, []Option[string]{Some("1")},
			[]scripted.Claim{func([]Option[string]) ([]scripted.Row, error) { return nil, unmatched }})
		return err
	})

	assert.IsError(t, err, unmatched)
}

// A script that is broken, returns rows MySQL cannot send, or loops forever, even in
// the getters of the rows it returns, cannot answer the candidate.
func TestStatementReplacementFailures(t *testing.T) {
	for name, test := range map[string]struct {
		using string
		err   string
	}{
		"Throws": {
			using: `() => { throw new Error("boom"); }`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception`,
		},
		"ReturnsObjects": {
			using: `() => [{ id: {} }] as unknown as Row[]`,
			err:   `result of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(): row field "id": value of type map[string]interface {} is not a string, number, boolean or null`,
		},
		"RunsTooLong": {
			using: `() => { for (;;) {} }`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception: context deadline exceeded at mysql.ts:2:63(1)`,
		},
		"ReadsRowsTooLong": {
			using: `() => [{ get id(): string { for (;;) {} } }]`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception: context deadline exceeded at id (mysql.ts:2:72(1))`,
		},
		"RowGetterThrows": {
			using: `() => [{ get id(): string { throw new Error("boom"); } }]`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			loaded, handle, err := load(t, `egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(`+test.using+`);`, 50*time.Millisecond)
			assert.NoError(t, err)

			err = callbacks(t, loaded, handle, func(replacement scripted.Replacement, callbacks scripted.Callbacks) error {
				_, err := callbacks.Combine(replacement, nil, nil)
				return err
			})

			assert.EqualError(t, err, test.err)
		})
	}
}

// Time that a script spends waiting for a claim is not counted against its timeout.
func TestWaitingForClaimsIsNotCounted(t *testing.T) {
	loaded, handle, err := load(t, `egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(
		([value], [claim]) => claim(value));`, 50*time.Millisecond)
	assert.NoError(t, err)

	var rows []scripted.Row
	err = callbacks(t, loaded, handle, func(replacement scripted.Replacement, callbacks scripted.Callbacks) error {
		var err error
		rows, err = callbacks.Combine(replacement, []Option[string]{Some("1")}, []scripted.Claim{func([]Option[string]) ([]scripted.Row, error) {
			time.Sleep(200 * time.Millisecond)
			return []scripted.Row{{"id": Some("1")}}, nil
		}})
		return err
	})
	assert.NoError(t, err)

	assert.Equal(t, []scripted.Row{{"id": Some("1")}}, rows)
}

// An evaluator that was not built with the binding has no callbacks to run.
func TestCallbacksOfOtherEvaluatorsAreUnavailable(t *testing.T) {
	loaded, handle := scripts(t, `egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(() => []);`)
	replacement := handle.Declared(loaded.Program()).Replacements[0]

	_, err := scripted.Callbacks{}.Combine(replacement, nil, nil)

	assert.EqualError(t, err, `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() is unavailable`)
}
