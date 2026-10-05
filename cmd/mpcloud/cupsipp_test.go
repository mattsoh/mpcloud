package main

import (
	"testing"
)

func TestDecodeIPPJobs(t *testing.T) {
	// A Get-Jobs response is shaped like a request, with job groups added.
	b := encodeIPP(0, []ippAttr{{tagCharset, "attributes-charset", []string{"utf-8"}}})
	b = b[:len(b)-1] // drop the end tag
	job := encodeIPP(0, []ippAttr{
		{tagInteger, "job-id", []string{"5"}},
		{tagEnum, "job-state", []string{"4"}},
		{tagKeyword, "job-state-reasons", []string{"job-incoming", "cups-held-for-authentication"}},
	})
	job[8] = tagJob
	b = append(b, job[8:]...)

	status, jobs, err := decodeIPP(b)
	if err != nil || status != 0 || len(jobs) != 1 {
		t.Fatalf("got status %d, %d jobs, err %v", status, len(jobs), err)
	}
	j := jobs[0]
	if j.id() != 5 || !j.waitingForLogin() {
		t.Fatalf("job decoded wrong: %v", j)
	}
}

func TestDecodeIPPShort(t *testing.T) {
	if _, _, err := decodeIPP([]byte{2, 0}); err == nil {
		t.Fatal("expected an error")
	}
}
