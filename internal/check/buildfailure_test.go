package check

import (
	"testing"

	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
	"github.com/amarbel-llc/tap/go/pkgs/ndjson"
)

// strPtr returns a pointer to s, used to populate TestRecord.Output.
func strPtr(s string) *string { return &s }

// TestBuildFailureSummary exercises the failing-record filter and
// formatting performed by buildFailureSummary. The function is the
// source of the failure summary emitted by runHookPhase in the
// format=tap-ndjson failure path.
func TestBuildFailureSummary(t *testing.T) {
	cases := []struct {
		name string
		in   ndjson.Output
		want string
	}{
		{
			name: "skip-excluded",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{N: 1, Description: "skipped", OK: false, Directive: &ndjson.DirectiveValue{Kind: "skip"}},
					{N: 2, Description: "passed", OK: true},
				},
			},
			want: "",
		},
		{
			name: "todo-excluded",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{N: 1, Description: "todo item", OK: false, Directive: &ndjson.DirectiveValue{Kind: "todo"}},
					{N: 2, Description: "passed", OK: true},
				},
			},
			want: "",
		},
		{
			name: "mixed-failure-and-skip",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{
						N:           1,
						Description: "real fail",
						OK:          false,
						Diagnostic:  map[string]any{"message": "msg"},
					},
					{
						N:           2,
						Description: "skipped",
						OK:          false,
						Directive:   &ndjson.DirectiveValue{Kind: "skip"},
					},
				},
			},
			want: "#1 real fail: msg",
		},
		{
			name: "non-string-diagnostic-message",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{
						N:           7,
						Description: "weird diag",
						OK:          false,
						Diagnostic:  map[string]any{"message": 42},
					},
				},
			},
			want: "#7 weird diag",
		},
		{
			name: "output-block-indentation",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{
						N:           3,
						Description: "with output",
						OK:          false,
						Diagnostic:  map[string]any{"message": "boom"},
						Output:      strPtr("line one\nline two\n"),
					},
				},
			},
			want: "#3 with output: boom\n  line one\n  line two",
		},
		{
			name: "all-skip-suite",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{N: 1, Description: "a", OK: false, Directive: &ndjson.DirectiveValue{Kind: "skip"}},
					{N: 2, Description: "b", OK: false, Directive: &ndjson.DirectiveValue{Kind: "skip"}},
					{N: 3, Description: "c", OK: false, Directive: &ndjson.DirectiveValue{Kind: "skip"}},
				},
			},
			want: "",
		},
		{
			name: "no-diagnostic",
			in: ndjson.Output{
				Records: []ndjson.TestRecord{
					{
						N:           9,
						Description: "bare failure",
						OK:          false,
						Diagnostic:  nil,
					},
				},
			},
			want: "#9 bare failure",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildFailureSummary(c.in)
			if got != c.want {
				t.Errorf("buildFailureSummary() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestBuildFailureSummaryCrap exercises the ndjson-crap analogue of
// buildFailureSummary: same genuine-failure filter (!OK && no directive)
// and the same one-line-per-failure formatting, over decoded
// ndjsoncrap.Test records instead of tap-ndjson TestRecords.
func TestBuildFailureSummaryCrap(t *testing.T) {
	cases := []struct {
		name string
		in   []ndjsoncrap.Test
		want string
	}{
		{
			name: "bare-failure",
			in: []ndjsoncrap.Test{
				{N: 9, Description: "bare failure", OK: false, Diagnostic: nil},
			},
			want: "#9 bare failure",
		},
		{
			name: "failure-with-message",
			in: []ndjsoncrap.Test{
				{
					N:           1,
					Description: "real fail",
					OK:          false,
					Diagnostic:  map[string]any{"message": "msg"},
				},
			},
			want: "#1 real fail: msg",
		},
		{
			name: "output-block-indentation",
			in: []ndjsoncrap.Test{
				{
					N:           3,
					Description: "with output",
					OK:          false,
					Diagnostic:  map[string]any{"message": "boom"},
					Output:      strPtr("line one\nline two\n"),
				},
			},
			want: "#3 with output: boom\n  line one\n  line two",
		},
		{
			name: "skip-excluded",
			in: []ndjsoncrap.Test{
				{N: 1, Description: "skipped", OK: false, Directive: &ndjsoncrap.Directive{Kind: "skip"}},
			},
			want: "",
		},
		{
			name: "todo-excluded",
			in: []ndjsoncrap.Test{
				{N: 1, Description: "todo item", OK: false, Directive: &ndjsoncrap.Directive{Kind: "todo"}},
			},
			want: "",
		},
		{
			name: "passing-excluded",
			in: []ndjsoncrap.Test{
				{N: 2, Description: "passed", OK: true},
			},
			want: "",
		},
		{
			name: "empty-input",
			in:   nil,
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildFailureSummaryCrap(c.in)
			if got != c.want {
				t.Errorf("buildFailureSummaryCrap() = %q, want %q", got, c.want)
			}
		})
	}
}
