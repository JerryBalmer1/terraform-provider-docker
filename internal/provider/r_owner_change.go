package provider

// Pass 5.3 Lane R: owner label for the image actions (inject-only; an action
// owns nothing in state, so there is no conflict check and no adopt).

import "strconv"

// rOwnerLabelChanges returns changes plus a Dockerfile `LABEL` instruction
// carrying the owner label, built from pd.ownerInject, when `owner` is set;
// changes unchanged otherwise. docker_image_commit (without `config`) and
// docker_image_import use it so the new image's config Labels carry the
// label.
func rOwnerLabelChanges(pd *providerData, changes []string) []string {
	v, ok := pd.ownerInject(nil)[ownerLabelKey]
	if !ok {
		return changes
	}
	out := append([]string(nil), changes...)
	return append(out, "LABEL "+strconv.Quote(ownerLabelKey)+"="+strconv.Quote(v))
}
