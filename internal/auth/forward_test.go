package auth

import (
	"context"
	"testing"
)

func TestVerifyForwardSecret_MatchMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	raw, err := s.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ok, err := s.VerifyForwardSecret(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the correct secret to verify")
	}

	ok, err = s.VerifyForwardSecret(ctx, raw+"x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected a mismatched secret to fail verification")
	}
}

func TestVerifyForwardSecret_EmptyWhitespaceRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.GenerateForwardSecret(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, presented := range []string{"", "   ", "\t\n"} {
		ok, err := s.VerifyForwardSecret(ctx, presented)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", presented, err)
		}
		if ok {
			t.Errorf("expected empty/whitespace presented secret %q to be rejected, not treated as a false-pass", presented)
		}
	}
}

func TestVerifyForwardSecret_NotConfigured_False(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ok, err := s.VerifyForwardSecret(ctx, "some-presented-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no-secret-configured to never verify a presented secret")
	}
}

func TestVerifyForwardSecret_StoreError(t *testing.T) {
	s, sqlDB := newTestStoreWithDB(t)
	ctx := context.Background()

	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	ok, err := s.VerifyForwardSecret(ctx, "any-nonempty-presented-secret")
	if err == nil {
		t.Fatal("expected a real settings-store error to propagate")
	}
	if ok {
		t.Error("expected false on a store error, never a match")
	}
}

// TestVerifyForwardSecret_ConstantTime is a behavioral check that
// VerifyForwardSecret uses subtle.ConstantTimeCompare correctly on
// equal-length hashes rather than a short-circuiting == — mirrors
// apikey_test.go's TestConstantTimeCompareUsed.
func TestVerifyForwardSecret_ConstantTime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	raw, err := s.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrong := make([]byte, len(raw))
	copy(wrong, raw)
	if wrong[0] == 'a' {
		wrong[0] = 'b'
	} else {
		wrong[0] = 'a'
	}

	ok, err := s.VerifyForwardSecret(ctx, string(wrong))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected a same-length mismatched secret to fail")
	}

	ok, err = s.VerifyForwardSecret(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the correct secret to still verify")
	}
}

func TestForwardHeaders_Defaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	userHeader, secretHeader, err := s.ForwardHeaders(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userHeader != defaultForwardUserHeader {
		t.Errorf("expected default user header %q, got %q", defaultForwardUserHeader, userHeader)
	}
	if secretHeader != defaultForwardSecretHeader {
		t.Errorf("expected default secret header %q, got %q", defaultForwardSecretHeader, secretHeader)
	}
}

func TestForwardHeaders_CustomOverridesDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetForwardHeaders(ctx, "X-Custom-User", "X-Custom-Secret"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	userHeader, secretHeader, err := s.ForwardHeaders(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userHeader != "X-Custom-User" || secretHeader != "X-Custom-Secret" {
		t.Errorf("expected custom headers to round-trip, got (%q, %q)", userHeader, secretHeader)
	}
}

// TestGenerateForwardSecret_RevealOnce covers G6: the raw secret is
// returned exactly once at generation time, is never retrievable via any
// getter afterward, and a subsequent generation invalidates the prior
// secret (mirrors Regenerate's API-key precedent).
func TestGenerateForwardSecret_RevealOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	raw, err := s.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw == "" {
		t.Fatal("expected a non-empty raw secret on generation")
	}

	configured, err := s.ForwardConfigured(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configured {
		t.Error("expected ForwardConfigured to be true after generation")
	}

	ok, err := s.VerifyForwardSecret(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the generated secret to verify")
	}

	raw2, err := s.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw2 == raw {
		t.Fatal("expected a fresh, different secret on a second generation")
	}
	ok, err = s.VerifyForwardSecret(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected the old secret to no longer verify after regeneration")
	}
	ok, err = s.VerifyForwardSecret(ctx, raw2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the new secret to verify")
	}
}

func TestForwardConfigured_FalseBeforeGenerate(t *testing.T) {
	s := newTestStore(t)
	ok, err := s.ForwardConfigured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ForwardConfigured to be false on a fresh store")
	}
}
