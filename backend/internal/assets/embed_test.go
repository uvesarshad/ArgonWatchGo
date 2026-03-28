package assets

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedAppUsesPageProtocolForWebSocket(t *testing.T) {
	frontendFS, err := GetFrontendAssets()
	if err != nil {
		t.Fatalf("GetFrontendAssets() error = %v", err)
	}

	content, err := fs.ReadFile(frontendFS, "js/app.js")
	if err != nil {
		t.Fatalf("ReadFile(js/app.js) error = %v", err)
	}

	source := string(content)

	if !strings.Contains(source, "window.location.protocol === 'https:' ? 'wss:' : 'ws:'") {
		t.Fatalf("embedded app.js does not derive websocket protocol from the current page protocol")
	}

	if strings.Contains(source, "new WebSocketClient(`ws://${window.location.host}/ws`)") {
		t.Fatalf("embedded app.js still hardcodes an insecure websocket URL")
	}
}
