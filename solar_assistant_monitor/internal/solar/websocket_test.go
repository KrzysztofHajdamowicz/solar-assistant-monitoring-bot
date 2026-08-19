package solar

import (
	"testing"
)

func TestDecodePhoenixData(t *testing.T) {
	payload := []byte(`["1",null,"metrics","data",{"metrics":[{"topic":"battery_1/current","value":1.2}]}]`)
	metrics, event, err := decodePhoenix(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event != "data" || len(metrics) != 1 || metrics[0].Topic != "battery_1/current" {
		t.Fatalf("unexpected decode: event=%s metrics=%+v", event, metrics)
	}
}

func TestDecodePhoenixReply(t *testing.T) {
	payload := []byte(`["1","1","metrics","phx_reply",{"status":"ok","response":{}}]`)
	metrics, event, err := decodePhoenix(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event != "phx_reply" || len(metrics) != 0 {
		t.Fatalf("unexpected decode: event=%s metrics=%+v", event, metrics)
	}
}
