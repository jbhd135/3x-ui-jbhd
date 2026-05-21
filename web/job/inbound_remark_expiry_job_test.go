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
			name:   "matches YYYYMMDD by month and day",
			remark: "20260521",
			now:    time.Date(2026, time.May, 21, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "matches either token in compound remark",
			remark: "0603.20270501",
			now:    time.Date(2026, time.May, 1, 10, 0, 0, 0, time.Local),
			want:   true,
		},
		{
			name:   "does not match a different day",
			remark: "0522",
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
