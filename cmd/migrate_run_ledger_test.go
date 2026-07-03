package cmd

import (
	"reflect"
	"testing"
)

func TestDiffLedgerKeySuffixesAllPresent(t *testing.T) {
	src := []string{"runs/9999999999-abc.json", "runs/8888888888-def.json"}
	dst := []string{"runledger/9999999999-abc.json", "runledger/8888888888-def.json"}
	missing := diffLedgerKeySuffixes(src, "runs/", dst, "runledger/")
	if len(missing) != 0 {
		t.Fatalf("want no missing keys, got %v", missing)
	}
}

func TestDiffLedgerKeySuffixesReportsMissing(t *testing.T) {
	src := []string{"runs/9999999999-abc.json", "runs/8888888888-def.json"}
	dst := []string{"runledger/9999999999-abc.json"}
	missing := diffLedgerKeySuffixes(src, "runs/", dst, "runledger/")
	want := []string{"8888888888-def.json"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("got %v, want %v", missing, want)
	}
}

func TestDiffLedgerKeySuffixesEmptySource(t *testing.T) {
	missing := diffLedgerKeySuffixes(nil, "runs/", nil, "runledger/")
	if len(missing) != 0 {
		t.Fatalf("want no missing keys for empty source, got %v", missing)
	}
}
