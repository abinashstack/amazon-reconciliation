package ingest

import (
	"testing"
	"time"
)

// --- error handling: date parsing --------------------------------------

func TestParsePaymentTime(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // formatted UTC, or "" for zero time
		wantErr bool
	}{
		{
			name: "the real reconciling example (README): GMT+9 -> UTC",
			in:   "17 July 2026 4:26:32 pm GMT+9",
			want: "2026-07-17T07:26:32Z",
		},
		{"negative offset", "1 January 2026 1:00:00 am GMT-5", "2026-01-01T06:00:00Z", false},
		{
			"real defect: Transaction Release Date sometimes abbreviates the month " +
				"(1153 of 20498 real rows use 'Aug' - 'date/time' never does this, only the release-date column)",
			"1 Aug 2026 4:59:36 am GMT+9", "2026-07-31T19:59:36Z", false,
		},
		{"blank is NOT an error - Deferred/unreleased rows leave this empty", "", "", false},
		{"whitespace-only is also blank", "   ", "", false},
		{"garbage with no GMT zone", "not a date at all", "", true},
		{"has a GMT marker but an unparseable body", "banana 2026 GMT+9", "", true},
		{"unparseable offset", "1 January 2026 1:00:00 am GMT+banana", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePaymentTime(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			gotStr := ""
			if !got.IsZero() {
				gotStr = got.UTC().Format("2006-01-02T15:04:05Z")
			}
			if gotStr != tc.want {
				t.Fatalf("got %q, want %q", gotStr, tc.want)
			}
		})
	}
}

func TestParseSettlementTime(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"the real format: DD.MM.YYYY HH:MM:SS UTC", "17.07.2026 07:26:32 UTC", "2026-07-17T07:26:32Z", false},
		{"date-only fallback", "17.07.2026", "2026-07-17T00:00:00Z", false},
		{"blank is not an error", "", "", false},
		{"garbage", "not-a-date", "", true},
		{"wrong separator (US-style, not this file's format)", "07/17/2026", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSettlementTime(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			gotStr := ""
			if !got.IsZero() {
				gotStr = got.UTC().Format("2006-01-02T15:04:05Z")
			}
			if gotStr != tc.want {
				t.Fatalf("got %q, want %q", gotStr, tc.want)
			}
		})
	}
}

func TestParseOffset(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"+9", 9 * 3600, false},
		{"-7", -7 * 3600, false},
		{"+09:30", 9*3600 + 30*60, false},
		{"-05:45", -(5*3600 + 45*60), false},
		{"banana", 0, true},
		{"+09:banana", 0, true}, // the specific bug: was silently swallowed pre-fix
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseOffset(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDateOnly(t *testing.T) {
	if got := dateOnly(time.Time{}); !got.IsZero() {
		t.Fatalf("zero time must stay zero, got %v", got)
	}
	full, err := parsePaymentTime("17 July 2026 4:26:32 pm GMT+9")
	if err != nil {
		t.Fatal(err)
	}
	got := dateOnly(full)
	want := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("dateOnly(%v) = %v, want %v", full, got, want)
	}
}

// --- error handling: amount parsing --------------------------------------

func TestParseAmount(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{"blank is not an error - most of a payments row's 11 amount columns are blank", "", 0, false},
		{"whitespace-only is also blank", "  ", 0, false},
		{"plain integer", "42", 42, false},
		{"decimal", "-133756.51", -133756.51, false},
		{"thousands separator, as Amazon reports sometimes use", "1,234.56", 1234.56, false},
		{"garbage IS an error - must not silently become 0", "N/A", 0, true},
		{"garbage IS an error", "#REF!", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAmount(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- error handling: warnings accumulator --------------------------------

func TestWarnings(t *testing.T) {
	var w warnings
	if w.Total() != 0 || len(w.Lines()) != 0 {
		t.Fatalf("empty warnings must report nothing")
	}
	for i := 0; i < 5; i++ {
		w.add("bad date", "line 1")
	}
	for i := 0; i < 3; i++ {
		w.add("bad amount", "line 2")
	}
	if got := w.Total(); got != 8 {
		t.Fatalf("Total() = %d, want 8 (exact count, not capped)", got)
	}
	lines := w.Lines()
	if len(lines) == 0 {
		t.Fatalf("expected non-empty Lines() once something was added")
	}

	t.Run("sample messages are capped so a pathological file can't blow up memory", func(t *testing.T) {
		var w2 warnings
		for i := 0; i < maxWarningSamples+50; i++ {
			w2.add("bad date", "line")
		}
		if int64(maxWarningSamples+50) != w2.Total() {
			t.Fatalf("Total() must stay exact even once sampling caps out: got %d", w2.Total())
		}
		if len(w2.samples) != maxWarningSamples {
			t.Fatalf("samples = %d, want capped at %d", len(w2.samples), maxWarningSamples)
		}
	})
}
