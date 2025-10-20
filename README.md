# ReKick

A command-line tool for downloading and archiving Kick VODs, including chat messages and emotes.
# Features

Downloads the VOD video file using yt-dlp.

Downloads the full chat log with all of their metadata (excessive?).

Downloads all emotes used in the chat.

Resumes downloads if interrupted.

Saves comprehensive metadata about the VOD.

Provides detailed progress display during download.

Warns about missing dependencies or low disk space.

Designed with graceful exits & recovery in mind (like, actually).

Highly configurable through command-line flags.

> **Disclaimer**
> Large parts of this code was generated with LLMs. While I vetted and edited it, it may contain unexpected bugs.
>
> **Also**, the json parsing for Kick's VOD html is a bit retarded, so PRs are welcome.

## Quick Start

### Prerequisites

1.  **Go**: Ensure you have a recent version of Go installed.
2.  **latest yt-dlp**: This tool is required for downloading the VOD video file. It must be installed and available in your system's PATH.
4.  **ffmpeg**: This is required for merging/post-processing from yt-dlp. It must be installed and available in your system's PATH


## Usage

`./rekick --url ""`
or if you like Bill Gates:
`rekick.exe --url ""`
(Put your VOD url inside the quotas)

The only required flag is the full URL to the Kick VOD.
This will create a new folder (e.g., channelname_12345678-a1c2-...) in the current directory containing the VOD, chat logs, and emotes.



#Command-Line Flags
```
Flag	Description	Default
--url                       (Required) The full URL of the Kick VOD to archive.	""
--output                    The base directory to save the archive folder into.	.
--simple-progress           Use a simple, single-line progress display without bars.	false
--ignore-disk-space         Ignore the low disk space warning and attempt to download anyway.	false
--no-vod                    Skip downloading the VOD video file.	false
--no-chat                   Skip downloading chat and emotes.	false
--no-emotes                 Download chat but skip downloading emotes.	false
--overwrite	                Delete and re-create the archive directory if it already exists.	false
--retries                   The number of times to retry a failed network request.	5
--ytdlp-retries	            The number of times to retry yt-dlp if it fails completely.	3
--chat-delay                Delay in milliseconds between chat API requests (min 100ms).	300
--max-concurrent-emotes	    The maximum number of emote download goroutines.	10
--quiet	                    Minimal output (errors and warnings only).	false
--debug	                    Enable debug output including raw API responses.	false
--ytdlp-verbose	            Show raw yt-dlp output (useful for debugging VOD download issues).	false
--log-file	                Optional path to a file for log output.	""
--no-emoji	                Disable emoji characters in log output for older terminals.	false
```

### Building

```sh
# Clone the repository
git clone https://github.com/VXsz/ReKick.git
cd rekick
go build
```
