package schema

import (
	"fmt"
	"strings"
	"testing"
)

// A schema shaped like the one that prompted this: a Tag points at a TagCategory, which
// points back at Tag (a loop), plus an enum, scalars and a repeated reference.
const tagSchema = `{"queryType":{"name":"Query"},"mutationType":{"name":"Mutation"},"types":[
 {"name":"Query","kind":"OBJECT","fields":[
   {"name":"tag","args":[{"name":"id","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}}],"type":{"kind":"OBJECT","name":"Tag"}},
   {"name":"tags","args":[],"type":{"kind":"LIST","ofType":{"kind":"OBJECT","name":"Tag"}}}]},
 {"name":"Mutation","kind":"OBJECT","fields":[
   {"name":"addTag","args":[{"name":"input","type":{"kind":"INPUT_OBJECT","name":"TagInput"}}],"type":{"kind":"OBJECT","name":"Tag"}}]},
 {"name":"Tag","kind":"OBJECT","fields":[
   {"name":"id","args":[],"type":{"kind":"SCALAR","name":"String"}},
   {"name":"category","args":[],"type":{"kind":"OBJECT","name":"TagCategory"}},
   {"name":"previous","args":[],"type":{"kind":"OBJECT","name":"TagCategory"}},
   {"name":"status","args":[],"type":{"kind":"NON_NULL","ofType":{"kind":"ENUM","name":"TagStatus"}}},
   {"name":"parent","args":[],"type":{"kind":"OBJECT","name":"Tag"}}]},
 {"name":"TagCategory","kind":"OBJECT","fields":[
   {"name":"id","args":[],"type":{"kind":"SCALAR","name":"String"}},
   {"name":"totalTags","args":[],"type":{"kind":"SCALAR","name":"Int"}},
   {"name":"tags","args":[],"type":{"kind":"LIST","ofType":{"kind":"OBJECT","name":"Tag"}}}]},
 {"name":"TagStatus","kind":"ENUM","enumValues":[{"name":"ACTIVE"},{"name":"ARCHIVED"}]},
 {"name":"TagInput","kind":"INPUT_OBJECT","inputFields":[
   {"name":"name","type":{"kind":"SCALAR","name":"String"}},
   {"name":"status","type":{"kind":"ENUM","name":"TagStatus"}}]},
 {"name":"String","kind":"SCALAR"},{"name":"Int","kind":"SCALAR"},{"name":"ID","kind":"SCALAR"}]}`

func tagSch(t *testing.T) Schema {
	t.Helper()
	s, err := Parse([]byte(tagSchema))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRelatedShowsWhatAFieldPointsAt(t *testing.T) {
	got, err := tagSch(t).Related("type", "Tag")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(got, "\n")

	// the case that prompted this: category: TagCategory
	for _, want := range []string{"related: type TagCategory", "  totalTags: Int", "related: enum TagStatus", "  ARCHIVED"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// exactly these, once each: no scalars (String, Int), no loop back to Tag itself
	// (TagCategory points back at it), and TagCategory listed once though two fields use it
	var headers []string
	for _, l := range got {
		if strings.HasPrefix(l, "related: ") {
			headers = append(headers, l)
		}
	}
	if strings.Join(headers, "|") != "related: type TagCategory|related: enum TagStatus" {
		t.Errorf("related headers = %q\n%s", headers, text)
	}
}

func TestRelatedForAQueryExpandsBeyondTheReturnedType(t *testing.T) {
	s := tagSch(t)
	// Detail of the query already prints Tag's fields; the new information is TagCategory
	got, err := s.Related("query", "tag")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(got, "\n")
	if !strings.Contains(text, "related: type TagCategory") || strings.Contains(text, "related: type Tag\n") {
		t.Errorf("query tag:\n%s", text)
	}
	// a mutation: the input type is already shown by Detail, so only what it points at is new
	got, err = s.Related("mutation", "addTag")
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Join(got, "\n")
	if strings.Contains(text, "related: input TagInput") || !strings.Contains(text, "related: enum TagStatus") {
		t.Errorf("mutation addTag:\n%s", text)
	}
}

func TestRelatedIsEmptyWhenThereIsNothingToAdd(t *testing.T) {
	got, err := tagSch(t).Related("enum", "TagStatus")
	if err != nil || len(got) != 0 {
		t.Fatalf("an enum points at nothing: %v %v", got, err)
	}
}

func TestRelatedRefusesWhatDetailRefuses(t *testing.T) {
	s := tagSch(t)
	if _, err := s.Related("type", "Nope"); err == nil {
		t.Error("an unknown type must be an error")
	}
	if _, err := s.Related("query", "nope"); err == nil || !strings.Contains(err.Error(), `no query named "nope"`) {
		t.Errorf("an unknown query: %v", err)
	}
}

func TestRelatedIsCapped(t *testing.T) {
	var types, fields []string
	for i := range MaxRelated + 3 {
		types = append(types, fmt.Sprintf(`{"name":"T%d","kind":"OBJECT","fields":[]}`, i))
		fields = append(fields, fmt.Sprintf(`{"name":"f%d","args":[],"type":{"kind":"OBJECT","name":"T%d"}}`, i, i))
	}
	big := fmt.Sprintf(`{"queryType":{"name":"Q"},"types":[{"name":"Q","kind":"OBJECT","fields":[]},{"name":"Hub","kind":"OBJECT","fields":[%s]},%s]}`,
		strings.Join(fields, ","), strings.Join(types, ","))
	s, err := Parse([]byte(big))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Related("type", "Hub")
	text := strings.Join(got, "\n")
	if n := strings.Count(text, "related: type"); n != MaxRelated {
		t.Errorf("listed %d related types, want the cap %d", n, MaxRelated)
	}
	if !strings.Contains(text, "... and 3 more related types") {
		t.Errorf("the truncation must say how many were left out:\n%s", text)
	}
	if e := s.Expand(mustType(t, s, "Hub")); len(e.Related) != MaxRelated || e.RelatedMore != 3 {
		t.Errorf("Expand: %d related, %d more", len(e.Related), e.RelatedMore)
	}
}

func mustType(t *testing.T, s Schema, name string) Type {
	t.Helper()
	ty, ok := s.FindType(name)
	if !ok {
		t.Fatalf("no type %s", name)
	}
	return ty
}

func TestExpandKeepsTheRenderedShapeAndAddsRelated(t *testing.T) {
	s := tagSch(t)
	e := s.Expand(mustType(t, s, "Tag"))
	if e.Name != "Tag" || len(e.Fields) != 5 {
		t.Fatalf("the type itself must come first and unchanged: %+v", e.Rendered)
	}
	var names []string
	for _, r := range e.Related {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "TagCategory,TagStatus" {
		t.Errorf("related = %v, want TagCategory,TagStatus in order of appearance", names)
	}
}
