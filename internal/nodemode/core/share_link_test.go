package core

import "testing"

func TestAnyTLSShareLinkKeepsInsecureTLSOption(t *testing.T) {
	outbound, err := outboundFromShareLink("anytls://password@example.com:443?security=tls&insecure=1&allowInsecure=1")
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, ok := outbound["tls"].(map[string]any)
	if !ok {
		t.Fatalf("expected tls config, got %+v", outbound)
	}
	if tlsConfig["insecure"] != true {
		t.Fatalf("expected tls.insecure=true, got %+v", tlsConfig)
	}
}
