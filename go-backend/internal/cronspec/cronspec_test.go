package cronspec

import "testing"

func TestValidateAcceptsOnlyFiveFieldExpressions(t *testing.T) {
	for _, ok := range []string{"0 3 * * *", "*/15 * * * *", "0 0 1 1 0", "30 2 * * 1-5"} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "* * * *", "0 0 * * * *", "@daily", "@every 1h", "61 * * * *", "nope"} {
		if Validate(bad) == nil || Valid(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestNewSchedulerUsesTheSameParser(t *testing.T) {
	c := New()
	if _, err := c.AddFunc("0 3 * * *", func() {}); err != nil {
		t.Fatalf("AddFunc(valid) = %v", err)
	}
	if _, err := c.AddFunc("@daily", func() {}); err == nil {
		t.Fatal("AddFunc(@daily) must be rejected by the five-field parser")
	}
}
