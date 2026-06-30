package worker

import "testing"

func TestNormalizeRunAsUser(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "runner", input: "runner", want: RunnerUser},
		{name: "root", input: "root", want: RootUser},
		{name: "uid", input: "5000", want: "5000:5000"},
		{name: "uid gid", input: "5000:6000", want: "5000:6000"},
		{name: "host rejected", input: "host", wantErr: true},
		{name: "invalid rejected", input: "nope", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRunAsUser(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRunAsUser(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
