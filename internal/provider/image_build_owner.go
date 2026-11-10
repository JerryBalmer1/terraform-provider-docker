package provider

// Pass 5.3 Lane B: the owner label on docker_image (build labels). A built
// image carries io.terraform.provider.owner=<owner> as an image label; a
// pulled or imported image cannot be labelled (its config, and so its ID,
// comes from the registry or the archive), so pulls are never labelled and
// never conflict-checked.
//
// The BuildKit path injects the label itself (imgBuildViaSeam). The classic
// path and the create/read/plan checks live in image_resource.go (Lane R),
// which calls the helpers below.

import (
	"context"

	"github.com/moby/moby/client"
)

// imgOwnerBuildLabels adds the owner label to classic build options.
func imgOwnerBuildLabels(pd *providerData, opts *client.ImageBuildOptions) {
	if opts != nil {
		opts.Labels = pd.ownerInject(opts.Labels)
	}
}

// imgOwnerCreateCheck fails a build whose name already exists as an image
// labelled with another owner. A missing image, or one without the label,
// is no conflict.
func imgOwnerCreateCheck(ctx context.Context, pd *providerData, name string) error {
	if pd == nil || pd.Owner == "" || pd.Client == nil || name == "" {
		return nil
	}
	res, err := pd.Client.ImageInspect(ctx, name)
	if err != nil || res.Config == nil {
		return nil
	}
	return ownerCreateCheck(pd, "image", name, res.Config.Labels)
}
