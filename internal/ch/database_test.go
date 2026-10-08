package ch

import "testing"

func TestDatabaseSQLPreservesLiterals(t *testing.T) {
	c := &Client{database: "app"}
	input := "SELECT * FROM lumen.events WHERE team_id = 'lumen.other' -- lumen.comment\n/* lumen.comment */"
	want := "SELECT * FROM app.events WHERE team_id = 'lumen.other' -- lumen.comment\n/* lumen.comment */"
	if got := c.databaseSQL(input); got != want {
		t.Fatalf("got %q", got)
	}
	for _, database := range []string{"", "lumen"} {
		if got := (&Client{database: database}).databaseSQL(input); got != input {
			t.Fatal("default changed")
		}
	}
}
func TestStripSQLComments(t *testing.T) {
	if got := stripSQLComments("-- init\nCREATE DATABASE IF NOT EXISTS lumen"); got != " \nCREATE DATABASE IF NOT EXISTS lumen" {
		t.Fatalf("got %q", got)
	}
}
