// Package renderqueue adapts the durable local scheduler to the public
// cutly_render render/status/cancel Agent actions. It queues immutable jobs but
// does not claim to execute workers, preflight projects, or render frames.
package renderqueue
