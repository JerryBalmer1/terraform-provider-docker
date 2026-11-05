package provider

// Pass 3 Lane E owns this file. Return every provider function constructor.

import "github.com/hashicorp/terraform-plugin-framework/function"

func laneFunctions() []func() function.Function {
	return []func() function.Function{
		NewParseImageRefFunction,
		NewImageRefFunction,
		NewComposeEnvFunction,
		NewPortSpecFunction,
	}
}
