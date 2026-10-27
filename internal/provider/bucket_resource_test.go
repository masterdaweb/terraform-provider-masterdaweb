package provider

import (
	"strings"
	"testing"
)

func TestBucketLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_object_storage_bucket")

	st := r.create(r.config(map[string]any{"name": "shop-files"}))
	api.expectBody("POST", "/v1/object-storage/buckets",
		`{"name":"shop-files","public":false,"deletion_protection":true,"quota_mode":"alert","abort_multipart_days":7}`)
	if str(t, st, "location") != "north-america" || !boolean(t, st, "deletion_protection") || !isNull(t, st, "public_url") ||
		len(strs(t, st, "cors")) != 0 {
		t.Fatalf("state %v", st)
	}

	// Rules as the API keeps them (trimmed, methods once each): no diff.
	cfg := map[string]any{
		"name": "shop-files", "public": true, "quota_bytes": 50_000_000_000, "quota_mode": "block", "abort_multipart_days": 0,
		"cors": []any{map[string]any{"allowed_origins": []string{" https://shop.example.com "}, "allowed_methods": []string{"GET", "PUT", "GET"},
			"max_age_seconds": 3600}},
		"lifecycle_rules": []any{map[string]any{"id": "tmp", "prefix": "tmp/", "expire_days": 7}},
	}
	api.resetCalls()
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", "/v1/object-storage/buckets/shop-files", `{"public":true,"quota_bytes":"50000000000","quota_mode":"block",
		"abort_multipart_days":null,
		"cors":[{"allowed_origins":[" https://shop.example.com "],"allowed_methods":["GET","PUT","GET"],"allowed_headers":[],"expose_headers":[],"max_age_seconds":3600}],
		"lifecycle":[{"id":"tmp","prefix":"tmp/","expire_days":7,"enabled":true}]}`)
	if str(t, st, "public_url") != "https://files.mwapp.com.br/shop-files" || num(t, st, "quota_bytes") != 50_000_000_000 ||
		num(t, st, "abort_multipart_days") != 0 || boolean(t, st, "lifecycle_rules", 0, "enabled") != true {
		t.Fatalf("state %v", st)
	}

	// Taking rules and the limit out clears them.
	api.resetCalls()
	st = r.update(st, r.config(map[string]any{"name": "shop-files", "public": true, "deletion_protection": false}))
	api.expectBody("PATCH", "/v1/object-storage/buckets/shop-files", `{"deletion_protection":false,"quota_bytes":null,
		"quota_mode":"alert","cors":[],"lifecycle":[],"abort_multipart_days":7}`)

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	del := api.callsTo("DELETE", "/v1/object-storage/buckets/shop-files")
	if len(del) != 1 || del[0].Query.Get("confirm") != "shop-files" {
		t.Fatalf("delete %+v", del)
	}
	// Deleted buckets are kept for a while, as deleted: gone for Terraform.
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a deleted bucket stayed: %v", got)
	}
}

func TestBucketProtectionImportAndRefusals(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_object_storage_bucket")
	cfg := r.config(map[string]any{"name": "shop-files", "lifecycle_rules": []any{map[string]any{"id": "all", "expire_days": 30, "enabled": false}}})
	st := r.create(cfg)
	expectError(t, r.destroy(st), "The bucket shop-files is protected from deletion")

	id := str(t, st, "id")
	for _, ref := range []string{"shop-files", id} {
		got, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		r.expectNoDiff(got, cfg)
	}

	p := r.plan(r.null(), r.config(map[string]any{"name": "shop-files"}))
	_, diags := r.apply(p)
	expectError(t, diags, "This bucket name is taken. Bucket names are shared by every account, so choose another. (HTTP 409, OBJECT_STORAGE_BUCKET_NAME_TAKEN")

	api.buckets["shop-files"].ID = "0193f0e2-0000-7000-8000-0000000000ff"
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("another bucket's state was taken: %v", got)
	}

	expectError(t, r.validate(r.config(map[string]any{"name": "Shop_Files"})), "lowercase")
	expectError(t, r.validate(r.config(map[string]any{"name": "ok-name", "quota_bytes": 10})), "quota_bytes")
	expectError(t, r.validate(r.config(map[string]any{"name": "ok-name",
		"cors": []any{map[string]any{"allowed_origins": []string{"*"}, "allowed_methods": []string{"get"}}}})), "allowed_methods")
	p = r.plan(r.null(), r.config(map[string]any{"name": "ok-name", "lifecycle_rules": []any{map[string]any{"id": "bad id", "expire_days": 1}}}))
	if !strings.Contains(diagText(append(p.diags, r.validate(r.config(map[string]any{"name": "ok-name",
		"lifecycle_rules": []any{map[string]any{"id": "bad id", "expire_days": 1}}}))...)), "dots, dashes or underscores") {
		t.Fatal("a bad rule id was accepted")
	}
}
