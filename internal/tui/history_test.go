package tui

import (
	"bytes"
	"reflect"
	"testing"
)

func TestHistoryWriterRecordsStyledPersistentOutput(t *testing.T) {
	var output bytes.Buffer
	history := newHistoryWriter(&output)
	input := "\x1b[31mhello\x1b[0m\r\n\rWaiting (|)\r\x1b[2Kworld\n"
	if _, err := history.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}

	want := []string{"\x1b[31mhello\x1b[0m", "world"}
	if got := history.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
	if output.String() != input {
		t.Fatal("styled output was not forwarded unchanged")
	}
}

func TestHistoryWriterHandlesSplitUTF8AndANSI(t *testing.T) {
	history := newHistoryWriter(&bytes.Buffer{})
	data := []byte("\x1b[2m想法\x1b[0m\n")
	for _, value := range data {
		if _, err := history.Write([]byte{value}); err != nil {
			t.Fatal(err)
		}
	}
	if got := history.Lines(); !reflect.DeepEqual(got, []string{"\x1b[2m想法\x1b[0m"}) {
		t.Fatalf("history = %#v", got)
	}
}

func TestHistoryWriterPreservesFinalStyleAfterProgressRewrite(t *testing.T) {
	history := newHistoryWriter(&bytes.Buffer{})
	input := "\x1b[33mWaiting\x1b[0m\r\x1b[2K\x1b[32mdone\x1b[0m\n"
	if _, err := history.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if got := history.Lines(); !reflect.DeepEqual(got, []string{"\x1b[32mdone\x1b[0m"}) {
		t.Fatalf("history = %#v", got)
	}
}

func TestHistoryPageMovesAndClamps(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5", "6", "7"}
	page, offset := historyPage(lines, 3, 0, 1)
	if !reflect.DeepEqual(page, []string{"2", "3", "4"}) || offset != 3 {
		t.Fatalf("first page = %v, offset = %d", page, offset)
	}
	page, offset = historyPage(lines, 3, offset, 1)
	if !reflect.DeepEqual(page, []string{"1", "2", "3"}) || offset != 4 {
		t.Fatalf("top page = %v, offset = %d", page, offset)
	}
	page, offset = historyPage(lines, 3, offset, -1)
	if !reflect.DeepEqual(page, []string{"4", "5", "6"}) || offset != 1 {
		t.Fatalf("down page = %v, offset = %d", page, offset)
	}
}

func TestVisualHistoryLinesUseFullWordWrap(t *testing.T) {
	history := newHistoryWriter(&bytes.Buffer{})
	history.AddLine("alpha beta gamma")
	u := UI{display: history, width: 10}
	if got := u.visualHistoryLines(); !reflect.DeepEqual(got, []string{"alpha beta", "gamma"}) {
		t.Fatalf("visual history = %#v", got)
	}
}
