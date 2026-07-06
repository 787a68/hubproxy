package handlers

import "testing"

func TestBuildAuthURL(t *testing.T) {
	tests := []struct {
		name        string
		authHost    string
		requestPath string
		want        string
	}{
		{
			name:        "host only appends request path",
			authHost:    "quay.io",
			requestPath: "/token",
			want:        "https://quay.io/token",
		},
		{
			name:        "configured path is preserved",
			authHost:    "ghcr.io/token",
			requestPath: "/token",
			want:        "https://ghcr.io/token",
		},
		{
			name:        "empty host falls back to docker auth",
			authHost:    "",
			requestPath: "/token",
			want:        "https://auth.docker.io/token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildAuthURL(tt.authHost, tt.requestPath); got != tt.want {
				t.Fatalf("buildAuthURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
