package provider

// docker_service_logs and docker_task_logs (DECISIONS Q6): snapshots of a
// service's aggregated task logs (ServiceLogs) and of one task's logs
// (TaskLogs), following the logs contract (log_entries.go): structured
// `log_entries`, raw `logs`, content_sha256, inputs `tail` / `since`.
// The entry source is the service name, or the task ID.

import (
	"context"
	"io"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

// swarmLogsInputs reads the tail and since inputs: the Engine query values
// and the tail count.
func swarmLogsInputs(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) (tail int64, tailQ, sinceQ string, vals map[string]attr.Value) {
	var t types.Int64
	var since types.String
	diags.Append(cfg.GetAttribute(ctx, path.Root("tail"), &t)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root("since"), &since)...)
	tail = logTailValue(t)
	sinceQ, err := logSinceQuery(since.ValueString(), time.Now())
	if err != nil {
		diags.AddAttributeError(path.Root("since"), "Invalid since", err.Error())
	}
	return tail, logTailQuery(tail), sinceQ, map[string]attr.Value{"tail": t, "since": since}
}

// swarmReadLogs reads one log stream and closes it.
func swarmReadLogs(rc io.ReadCloser, tty bool, source string, tail int64) ([]logEntry, error) {
	defer rc.Close()
	entries, err := logReadEntries(rc, tty, source)
	if err != nil {
		return nil, err
	}
	// The Engine applies tail per task; the service stream interleaves
	// tasks, so order by time and keep the last tail lines overall.
	return logApplyTail(logMerge(entries), tail), nil
}

func swarmLogsAttrs(target, targetDesc string) func() map[string]schema.Attribute {
	return func() map[string]schema.Attribute {
		return map[string]schema.Attribute{
			target:  schema.StringAttribute{Required: true, MarkdownDescription: targetDesc},
			"id":    schema.StringAttribute{Computed: true, MarkdownDescription: "Full ID the logs were read from."},
			"tail":  logTailAttribute(),
			"since": logSinceAttribute(),
		}
	}
}

// NewServiceLogsDataSource returns the docker_service_logs data source.
func NewServiceLogsDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "service_logs", itemID: "swarm:service-logs",
		desc: "Reads a snapshot of a swarm service's logs, aggregated over its tasks (`GET /services/{id}/logs`), like `docker service logs`. " +
			"`log_entries` has one object per line (`timestamp`, `stream`, `source` = the service name, `message`, which alone is sensitive) and " +
			"`logs` the same as text. `tail` (default 1000) and `since` bound what is read; the provider's host must be a swarm manager.",
		attrs: swarmLogsAttrs("service_id", "Service ID, unique ID prefix or name."),
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, _ := swarmDSRef(ctx, cfg, diags, "service_id")
			tail, tailQ, sinceQ, vals := swarmLogsInputs(ctx, cfg, diags)
			if diags.HasError() {
				return nil, contractFillInput{}, false
			}
			id, err := resolveServiceID(ctx, pd.Client, ref)
			if err != nil {
				diags.AddAttributeError(path.Root("service_id"), "Cannot find service", err.Error())
				return nil, contractFillInput{}, false
			}
			sres, err := pd.Client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect service", err.Error())
				return nil, contractFillInput{}, false
			}
			tty := false
			if cs := sres.Service.Spec.TaskTemplate.ContainerSpec; cs != nil {
				tty = cs.TTY
			}
			rc, err := pd.Client.ServiceLogs(ctx, id, client.ServiceLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: tailQ, Since: sinceQ})
			if err != nil {
				diags.AddError("Cannot read service logs", err.Error())
				return nil, contractFillInput{}, false
			}
			entries, err := swarmReadLogs(rc, tty, sres.Service.Spec.Name, tail)
			if err != nil {
				diags.AddError("Cannot read service logs", err.Error())
				return nil, contractFillInput{}, false
			}
			vals["service_id"] = types.StringValue(ref)
			vals["id"] = types.StringValue(id)
			return vals, logFillInput(entries), true
		},
	}
}

// NewTaskLogsDataSource returns the docker_task_logs data source.
func NewTaskLogsDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "task_logs", itemID: "swarm:task-logs",
		desc: "Reads a snapshot of one swarm task's logs (`GET /tasks/{id}/logs`), like `docker service logs <task id>`. " +
			"`log_entries` has one object per line (`timestamp`, `stream`, `source` = the task ID, `message`, which alone is sensitive) and " +
			"`logs` the same as text. `tail` (default 1000) and `since` bound what is read; the provider's host must be a swarm manager.",
		attrs: swarmLogsAttrs("task_id", "Task ID (see `docker_tasks`)."),
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, _ := swarmDSRef(ctx, cfg, diags, "task_id")
			tail, tailQ, sinceQ, vals := swarmLogsInputs(ctx, cfg, diags)
			if diags.HasError() {
				return nil, contractFillInput{}, false
			}
			tres, err := pd.Client.TaskInspect(ctx, ref, client.TaskInspectOptions{})
			if err != nil {
				diags.AddAttributeError(path.Root("task_id"), "Cannot inspect task", err.Error())
				return nil, contractFillInput{}, false
			}
			t := tres.Task
			tty := false
			if cs := t.Spec.ContainerSpec; cs != nil {
				tty = cs.TTY
			}
			rc, err := pd.Client.TaskLogs(ctx, t.ID, client.TaskLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: tailQ, Since: sinceQ})
			if err != nil {
				diags.AddError("Cannot read task logs", err.Error())
				return nil, contractFillInput{}, false
			}
			entries, err := swarmReadLogs(rc, tty, t.ID, tail)
			if err != nil {
				diags.AddError("Cannot read task logs", err.Error())
				return nil, contractFillInput{}, false
			}
			vals["task_id"] = types.StringValue(ref)
			vals["id"] = types.StringValue(t.ID)
			return vals, logFillInput(entries), true
		},
	}
}
