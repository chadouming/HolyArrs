// Package transcribe makes the timed lines of a subtitle from a video's
// audio. It runs ffmpeg to read one audio track and whisper.cpp's
// command-line program to transcribe it, both as child processes that end
// with HolyArrs. The spoken language is checked on three samples before the
// whole track is transcribed, so a wrong language tag never labels the result.
package transcribe
