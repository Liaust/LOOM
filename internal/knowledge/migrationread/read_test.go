package migrationread

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestDeclarationMigrationKnowledgeRequiresTransaction(t *testing.T) {
	out, err := ReadDeclarationMigrationKnowledgeTx(t.Context(), nil, "project", "node")
	if err == nil || out.Completeness != "unavailable" || out.Revision != "" {
		t.Fatal("missing transaction asserted completeness")
	}
}

type migrationSourceScanner struct{}

func (migrationSourceScanner) Scan(dest ...any) error {
	for i, p := range dest {
		switch v := p.(type) {
		case *string:
			*v = "field-" + fmt.Sprint(i)
		case *sql.NullString:
			*v = sql.NullString{Valid: i != 4, String: " nullable-" + fmt.Sprint(i) + " "}
		case *[]byte:
			*v = []byte(fmt.Sprintf(`{"field":%d}`, i))
		case *time.Time:
			*v = time.Unix(int64(i), 0).UTC()
		default:
			return fmt.Errorf("unexpected destination %T", p)
		}
	}
	return nil
}
func TestDeclarationMigrationKnowledgeExactPrivateFields(t *testing.T) {
	v, e := scanSourceRoot(migrationSourceScanner{})
	if e != nil {
		t.Fatal(e)
	}
	if v.ProjectID != nil || v.NodeID == nil || *v.NodeID != " nullable-2 " || v.NotesSourceRootID != "field-0" || v.Status != "field-11" || string(v.AuthorizationMetadata) != `{"field":12}` || string(v.Metadata) != `{"field":13}` || !v.CreatedAt.Equal(time.Unix(14, 0)) {
		t.Fatalf("private owner fields normalized or omitted: %+v", v)
	}
}
