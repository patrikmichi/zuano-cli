package recipes

// Change is a field that differs between two versions.
type Change[T any] struct {
	From T `json:"from"`
	To   T `json:"to"`
}

// Difficulty is how hard a recipe is and why.
type Difficulty struct {
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

// Moved lists what a version added and removed from a list of tools.
type Moved struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// StepChange is one step that was added, removed or changed; the side that
// does not exist is nil.
type StepChange struct {
	Order  int    `json:"order"`
	Change string `json:"change"`
	Before *Step  `json:"before"`
	After  *Step  `json:"after"`
}

// PitfallChanges lists the pitfalls a version added and removed.
type PitfallChanges struct {
	Added   []Pitfall `json:"added"`
	Removed []Pitfall `json:"removed"`
}

// Diff is the gateway's comparison of two versions of a recipe. Nothing signs
// it, so it informs a reader and is never a reason to trust a version.
type Diff struct {
	RecipeID     string              `json:"recipeId"`
	Against      string              `json:"against"`
	From         int                 `json:"from"`
	To           int                 `json:"to"`
	Changed      bool                `json:"changed"`
	Title        *Change[string]     `json:"title"`
	UseCase      *Change[string]     `json:"useCase"`
	Difficulty   *Change[Difficulty] `json:"difficulty"`
	PrimaryTools Moved               `json:"primaryTools"`
	RelatedTools Moved               `json:"relatedTools"`
	Steps        []StepChange        `json:"steps"`
	Pitfalls     PitfallChanges      `json:"pitfalls"`
}
