-- Every playback decision looks up what this device refused for this file.
CREATE INDEX playback_session_device_file ON playback_session(device_id, media_file_id);
