// Package builtin runs a translation model inside HolyArrs's own container:
// it starts the llama.cpp server as a child process on a loopback address,
// only while translations run, and stops it when idle. The process speaks the
// OpenAI chat API, so the calls themselves are translate.OpenAI's.
package builtin
