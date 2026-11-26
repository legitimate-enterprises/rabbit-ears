CREATE INDEX session_plan_entry_audio_track ON session_plan_entry(audio_track_id);
CREATE INDEX session_plan_entry_subtitle_track ON session_plan_entry(subtitle_track_id);
CREATE INDEX playback_session_media_file ON playback_session(media_file_id);
CREATE INDEX watch_state_item ON watch_state(item_id);
CREATE INDEX watch_state_edition ON watch_state(edition_id);
