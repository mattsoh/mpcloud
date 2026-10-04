package main

import "testing"

func TestParseCUPSOptions(t *testing.T) {
	o := parseCUPSOptions(`job-uuid=urn:uuid:1 Duplex=DuplexNoTumble PageSize=A4 job-name='my doc' x=a\ b collate`)
	want := map[string]string{"job-uuid": "urn:uuid:1", "Duplex": "DuplexNoTumble", "PageSize": "A4", "job-name": "my doc", "x": "a b", "collate": "true"}
	for k, v := range want {
		if o[k] != v {
			t.Errorf("%s = %q, want %q", k, o[k], v)
		}
	}
	if q := queueName("2nd Floor Color Printer Mobility Queue"); q != "2nd-Floor-Color-Printer" {
		t.Errorf("queueName = %q", q)
	}
}
