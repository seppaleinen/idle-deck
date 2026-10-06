package tracker

// Label vocabulary (D29). All labels are lowercase, hyphenated, idle- prefixed.
// The prefix is a collision-safety property, not style.
const (
	LabelHotfix     = "idle-hotfix"
	LabelReady      = "idle-ready"
	LabelRedo       = "idle-redo"
	LabelNeedsHuman = "idle-needs-human"
)
