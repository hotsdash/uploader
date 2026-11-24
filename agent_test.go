package main

import (
	"reflect"
	"testing"
)

func TestBattletagsFrom(t *testing.T) {
	b := []byte("\x00\x01LeoGold#2966\x00\x05Ernést#2208\x00LeoGold#2966 1234#5678 x#12 Bucko#21496\x00")
	got := BattletagsFrom(b)
	want := []string{"LeoGold#2966", "Ernést#2208", "Bucko#21496"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
