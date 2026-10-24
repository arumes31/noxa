"""Publishing must validate every listening approval before replacing assets."""
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("speech", Path(__file__).with_name("generate-supertonic-speech.py"))
speech = importlib.util.module_from_spec(spec)
spec.loader.exec_module(speech)


class PublishReviewTests(unittest.TestCase):
    def run_publish(self, note, *, accepted=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            out, assets = root / "candidates", root / "assets"
            (out / "clips/de").mkdir(parents=True)
            (assets / "de").mkdir(parents=True)
            clips, reviews = {}, {}
            for event in ("day", "night"):
                key = "de/" + event
                (out / "clips" / (key + ".wav")).write_bytes(b"new " + event.encode())
                (assets / (key + ".wav")).write_bytes(b"original")
                clips[key] = {"sha256": speech.sha256(out / "clips" / (key + ".wav"))}
                reviews[key] = {**clips[key], "reviewStatus": "accepted-automatic-content-review"}
            reviews["de/night"]["reviewStatus"] = "accepted-user-listening-review"
            if note is not None:
                reviews["de/night"]["note"] = note
            (out / "manifest.json").write_text(json.dumps({"clips": clips}))
            (out / "st13-content-review.json").write_text(json.dumps(reviews))
            with patch.object(speech, "ASSETS", assets), patch.object(speech, "LINES", {"de": {"day": "Day", "night": "Night"}}):
                if accepted:
                    speech.publish(SimpleNamespace(out=out))
                    self.assertEqual((assets / "de/night.wav").read_bytes(), b"new night")
                    self.assertEqual((assets / "de/day.wav").read_bytes(), b"new day")
                else:
                    with self.assertRaisesRegex(SystemExit, "listening approval"):
                        speech.publish(SimpleNamespace(out=out))
                    # A later invalid approval must not partially publish earlier clips.
                    self.assertEqual((assets / "de/day.wav").read_bytes(), b"original")
                    self.assertEqual((assets / "de/night.wav").read_bytes(), b"original")

    def test_missing_or_empty_note_rejects_without_publishing(self):
        for note in (None, "", "  "):
            with self.subTest(note=note):
                self.run_publish(note, accepted=False)

    def test_documented_listening_approval_publishes(self):
        self.run_publish("User selected this exact recording after listening.")


if __name__ == "__main__":
    unittest.main()
