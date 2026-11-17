package repoassess

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Declaration is the user's own answers about the repository, one
// option of each Question. They are what the user says, not something
// this package measures.
type Declaration struct {
	Ownership    string `json:"ownership"`
	EverPublic   string `json:"ever_public"`
	InProduction string `json:"in_production"`
	AIShare      string `json:"ai_share"`
}

// Question is one closed question a submission asks. Key is the name
// of its answer in a Declaration: the JSON name of the field answer
// points to, so the name is spelled once, in the field's tag.
type Question struct {
	Key     string
	Text    string
	Options []Option
	answer  func(*Declaration) *string
}

// Option is one answer to a Question. An option that Stops is never
// sent: choosing it ends the submission before anything is sent.
type Option struct {
	Value string
	Text  string
	Stops bool
}

// Questions are the questions a submission asks, in the order they are
// asked. Ownership comes first, so a user who may not share the
// repository is stopped before anything else is asked.
var Questions = keyed([]Question{
	{
		answer: func(d *Declaration) *string { return &d.Ownership },
		Text:   "Who owns this repository?",
		Options: []Option{
			{Value: "sole_author", Text: "I wrote it on my own"},
			{Value: "team_with_consent", Text: "A team wrote it, and I can get the consent of everyone on the team"},
			{Value: "company_authorized", Text: "It belongs to a company, and I can act for the company"},
			{Value: "not_mine", Text: "Someone else owns it, and I cannot act for them", Stops: true},
			{Value: "not_sure", Text: "I am not sure", Stops: true},
		},
	},
	{
		answer: func(d *Declaration) *string { return &d.EverPublic },
		Text:   "Was this code ever public?",
		Options: []Option{
			{Value: "never", Text: "Never"},
			{Value: "partly", Text: "Part of it was public at some time"},
			{Value: "fully", Text: "All of it was public at some time"},
		},
	},
	{
		answer: func(d *Declaration) *string { return &d.InProduction },
		Text:   "Does this software run in production?",
		Options: []Option{
			{Value: "paying_users", Text: "Yes, with paying users"},
			{Value: "real_users", Text: "Yes, with users who do not pay"},
			{Value: "internal_only", Text: "Only inside an organization"},
			{Value: "not_launched", Text: "No, it has not launched"},
		},
	},
	{
		answer: func(d *Declaration) *string { return &d.AIShare },
		Text:   "About how much of the code did an AI write?",
		Options: []Option{
			{Value: "under_5", Text: "Less than 5%"},
			{Value: "5_to_30", Text: "5% to 30%"},
			{Value: "30_to_70", Text: "30% to 70%"},
			{Value: "over_70", Text: "More than 70%"},
			{Value: "unsure", Text: "I do not know"},
		},
	},
})

// keyed sets the Key of each question to the JSON name of the
// Declaration field it answers.
func keyed(questions []Question) []Question {
	var d Declaration
	fields := reflect.ValueOf(&d).Elem()
	for i, q := range questions {
		at := reflect.ValueOf(q.answer(&d)).Pointer()
		for f := range fields.NumField() {
			if fields.Field(f).Addr().Pointer() == at {
				questions[i].Key, _, _ = strings.Cut(fields.Type().Field(f).Tag.Get("json"), ",")
			}
		}
		if questions[i].Key == "" {
			panic("repoassess: a question answers no field of Declaration")
		}
	}
	return questions
}

// Option finds the option of q whose value is value.
func (q Question) Option(value string) (Option, bool) {
	i := slices.IndexFunc(q.Options, func(o Option) bool { return o.Value == value })
	if i < 0 {
		return Option{}, false
	}
	return q.Options[i], true
}

// Values lists the values q accepts, for a message that names them.
func (q Question) Values() string {
	values := make([]string, len(q.Options))
	for i, o := range q.Options {
		values[i] = o.Value
	}
	return strings.Join(values, ", ")
}

// NewDeclaration builds a Declaration from an answer to every question,
// by key. Each answer must be an option of its question that does not
// stop.
func NewDeclaration(answers map[string]string) (Declaration, error) {
	var d Declaration
	for _, q := range Questions {
		o, ok := q.Option(answers[q.Key])
		if !ok || o.Stops {
			return Declaration{}, fmt.Errorf("%s: %q is not an answer that can be sent", q.Key, answers[q.Key])
		}
		*q.answer(&d) = o.Value
	}
	return d, nil
}
