// Package renderqueue adapts the durable local scheduler to the public
// cutly_render render/status/cancel Agent actions. New retains the unscoped
// embedding API; daemon hosts should use NewScoped so all actions are confined
// to one project and every immutable job passes prepared-environment admission
// before it becomes durable. The package does not execute workers or frames.
package renderqueue
