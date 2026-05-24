package job

import (
	"testing"
	"time"
)

func TestRemarkDateDue(t *testing.T) {
	tests := []struct {
		name   string
		remark string
		now    time.Time
		want   bool
	}{
		{
			name:   "matches MMDD",
			remark: "0521",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "matches YYYYMMDD in current year",
			remark: "20260521",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "keeps future-year YYYYMMDD",
			remark: "20270520",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "keeps future-year YYMMDD",
			remark: "270520",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "matches YYMMDD in current year",
			remark: "260521",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "keeps compound remark with future dates",
			remark: "0603.20270501",
			now:    time.Date(2026, time.May, 1, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "deletes already expired MMDD",
			remark: "0520",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "deletes already expired YYYYMMDD",
			remark: "20260519",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "deletes previous month date",
			remark: "0431",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "keeps future day in current month",
			remark: "0522",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "keeps future month date",
			remark: "0601",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "non leap February last day catches 0228",
			remark: "0228",
			now:    time.Date(2026, time.February, 28, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "non leap February last day catches 0229",
			remark: "0229",
			now:    time.Date(2026, time.February, 28, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "non leap February last day catches 0230",
			remark: "0230",
			now:    time.Date(2026, time.February, 28, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "non leap February last day catches 0231",
			remark: "0231",
			now:    time.Date(2026, time.February, 28, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "non last day does not catch future invalid date",
			remark: "0230",
			now:    time.Date(2026, time.February, 27, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "leap February 28 does not catch 0229 early",
			remark: "0229",
			now:    time.Date(2028, time.February, 28, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "leap February last day catches 0230",
			remark: "0230",
			now:    time.Date(2028, time.February, 29, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "April last day catches 0431",
			remark: "0431",
			now:    time.Date(2026, time.April, 30, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "June last day catches 0631",
			remark: "0631",
			now:    time.Date(2026, time.June, 30, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "June before last day keeps 0631",
			remark: "0631",
			now:    time.Date(2026, time.June, 29, 10, 0, 0, 0, time.Local),
			want:   false,
		},
		{
			name:   "rejects invalid month",
			remark: "1321",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remarkDateDue(tt.remark, tt.now); got != tt.want {
				t.Fatalf("remarkDateDue(%q, %s) = %v, want %v", tt.remark, tt.now.Format(time.DateOnly), got, tt.want)
			}
		})
	}
}
