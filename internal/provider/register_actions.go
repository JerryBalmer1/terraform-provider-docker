package provider

// Pass 3 Lane D owns this file. Return every action constructor.

import "github.com/hashicorp/terraform-plugin-framework/action"

func laneActions() []func() action.Action {
	return []func() action.Action{
		NewExecAction,
		NewContainerExportAction,
		NewImageImportAction,
		NewImageLoadAction,
		NewImageSaveAction,
		NewSystemPruneAction,
		NewImagePruneAction,
		NewSwarmRotateTokensAction,
		NewNodeDrainAction,
		NewNodeActivateAction,
		NewServiceScaleAction,
		NewServiceForceUpdateAction,
		NewServiceRollbackAction,
	}
}
