package tools

import "testing"

func TestSanitizeToolName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"orders.list", "orders_list"},
		{"users.getMe", "users_getMe"},
		{"plain_name", "plain_name"},
		{"with-dash", "with-dash"},
		{"", "tool"},
		{"...", "___"},
	}
	for _, c := range cases {
		if got := sanitizeToolName(c.in); got != c.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
