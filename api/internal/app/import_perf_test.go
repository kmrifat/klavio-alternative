package app_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestImportPerformance: IMPORT_PERF_ROWS=50000 go test -run TestImportPerformance ./internal/app/
func TestImportPerformance(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("IMPORT_PERF_ROWS"))
	if n == 0 {
		t.Skip("set IMPORT_PERF_ROWS to run")
	}
	a, srv, dns := setup(t)
	owner := newBrowser(t, srv.URL)
	link, _ := a.Bootstrap(context.Background(), "owner@acme.test", "Olivia Owner")
	owner.must(200, owner.acceptInvite(link))
	owner.must(201, owner.do("POST", "/admin/v1/brands", brightside))
	base := "/admin/v1/brands/brightside"
	topic := owner.must(200, owner.do("GET", base+"/topics", nil)).body["topics"].([]any)[0].(map[string]any)["id"].(string)

	var sb strings.Builder
	sb.WriteString("email,first_name,confirm_time\n")
	date := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	for i := range n {
		d := fmt.Sprintf("d%d.com", i%500)
		dns.mx[d] = []*net.MX{{Host: "mx." + d + ".", Pref: 10}}
		fmt.Fprintf(&sb, "user%d@%s,User %d,%s\n", i, d, i, date)
	}

	start := time.Now()
	up := owner.must(201, owner.upload(base+"/imports", "big.csv", sb.String()))
	imp := up.body["import"].(map[string]any)
	id := imp["id"].(string)
	owner.must(200, owner.do("PUT", base+"/imports/"+id, map[string]any{
		"mapping": imp["mapping"], "topic_ids": []string{topic},
		"consent": map[string]any{"source": "website_form", "collected_period": "2026", "wording": "x", "legal_basis": "consent", "attested": true},
	}))
	t.Logf("upload+setup: %v", time.Since(start))
	start = time.Now()
	owner.must(202, owner.do("POST", base+"/imports/"+id+"/check", nil))
	t.Logf("check: %v", time.Since(start))
	start = time.Now()
	done := owner.must(202, owner.do("POST", base+"/imports/"+id+"/commit", nil))
	t.Logf("commit: %v → %v", time.Since(start), done.body["import"].(map[string]any)["import_result"])
}
