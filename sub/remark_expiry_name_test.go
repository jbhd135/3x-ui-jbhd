package sub

import (
	"fmt"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

func TestFormatClientExpiryRemark(t *testing.T) {
	now := time.Date(2026, time.May, 24, 10, 0, 0, 0, time.Local)
	tests := []struct {
		name   string
		remark string
		want   string
		ok     bool
	}{
		{
			name:   "formats current year MMDD",
			remark: "0615",
			want:   "过期日期:2026年6月15日",
			ok:     true,
		},
		{
			name:   "formats explicit year YYYYMMDD",
			remark: "20270524",
			want:   "过期日期:2027年5月24日",
			ok:     true,
		},
		{
			name:   "formats short year YYMMDD",
			remark: "270524",
			want:   "过期日期:2027年5月24日",
			ok:     true,
		},
		{
			name:   "clamps invalid month day",
			remark: "0631",
			want:   "过期日期:2026年6月30日",
			ok:     true,
		},
		{
			name:   "clamps non leap February",
			remark: "0230",
			want:   "过期日期:2026年2月28日",
			ok:     true,
		},
		{
			name:   "uses earliest date token",
			remark: "0603.20270501",
			want:   "过期日期:2026年6月3日",
			ok:     true,
		},
		{
			name:   "ignores non date remark",
			remark: "vip-node",
			ok:     false,
		},
		{
			name:   "ignores invalid month",
			remark: "1321",
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := formatClientExpiryRemark(tt.remark, now)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("formatClientExpiryRemark(%q) = %q, %v; want %q, %v", tt.remark, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestGenRemarkHidesClientEmailForExpiryRemark(t *testing.T) {
	service := &SubService{remarkModel: "-ieo"}
	inbound := &model.Inbound{Remark: "0615"}

	got := service.genRemark(inbound, "ayd3o269", "")
	want := fmt.Sprintf("过期日期:%d年6月15日", time.Now().Year())
	if got != want {
		t.Fatalf("genRemark() = %q, want %q", got, want)
	}
}
