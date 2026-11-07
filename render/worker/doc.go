// Package worker defines Cutly's local render worker process protocol and a
// host-owned executable runner. It provides crash and cancellation isolation;
// it is not an OS sandbox and does not grant an executable permission to read
// arbitrary untrusted paths or use the network.
package worker
