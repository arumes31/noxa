import test from "node:test";
import assert from "node:assert/strict";
import { videoPortal, portalThumbnail } from "../src/video-portal-previews.js";

test("Twitch stream, VOD, clip and player URLs normalize to public resources", () => {
 for (const [url,id] of [["https://www.twitch.tv/twitch","channel:twitch"],["https://www.twitch.tv/videos/123456","video:123456"],["https://clips.twitch.tv/PublicClip_123","clip:PublicClip_123"],["https://www.twitch.tv/streamer/clip/PublicClip_123","clip:PublicClip_123"],["https://clips.twitch.tv/embed?clip=PublicClip_123&parent=localhost","clip:PublicClip_123"],["https://player.twitch.tv/?video=v123456&parent=localhost","video:123456"]]) assert.equal(videoPortal(url)?.id,id,url);
});
test("Vimeo and Dailymotion public URLs and short links normalize", () => {
 for (const url of ["https://vimeo.com/22439234","https://player.vimeo.com/video/22439234","https://vimeo.com/channels/staffpicks/22439234"]) assert.equal(videoPortal(url)?.url,"https://vimeo.com/22439234");
 for (const url of ["https://dai.ly/x84sh87","https://www.dailymotion.com/video/x84sh87_title","https://www.dailymotion.com/embed/video/x84sh87"]) assert.equal(videoPortal(url)?.url,"https://www.dailymotion.com/video/x84sh87");
});
test("Whatnot show, share, profile and home links are supported", () => {
 for (const [url,id] of [["https://whatnot.com/","home"],["https://www.whatnot.com/s/Zz64h7wC","s:Zz64h7wC"],["https://www.whatnot.com/user/whatnot","user:whatnot"],["https://www.whatnot.com/live/59f6f43b-0b3f-4181-b873-2c7b4d31f77f?sharing_channel=copyLink","live:59f6f43b-0b3f-4181-b873-2c7b4d31f77f"]]) assert.equal(videoPortal(url)?.id,id,url);
});
test("portal parser rejects unrelated hosts, credentials, invalid IDs and non-video pages", () => {
 for (const url of ["https://twitch.tv.evil.test/twitch","https://user@www.twitch.tv/twitch","https://twitch.tv:8443/twitch","https://twitch.tv/directory","https://vimeo.com/0","https://vimeo.com/login","https://dai.ly/x84sh87%2Fprivate","https://www.whatnot.com/api/private","https://www.whatnot.com/live/not-a-uuid","https://whatnot.com.evil.test/s/Zz64h7wC","javascript:alert(1)",null]) assert.equal(videoPortal(url),null,String(url));
});
test("only official provider image CDNs are accepted", () => {
 for (const [provider,url] of [["twitch","https://static-cdn.jtvnw.net/image.jpg"],["vimeo","https://i.vimeocdn.com/video/image.jpg"],["dailymotion","https://s2.dmcdn.net/image.jpg"],["whatnot","https://images.whatnot.com/image.jpg"]]) assert.equal(portalThumbnail(provider,url),url);
 for (const url of ["http://images.whatnot.com/image.jpg","https://images.whatnot.com.evil.test/image.jpg","https://user@images.whatnot.com/image.jpg","https://images.whatnot.com:8443/image.jpg","https://127.0.0.1/image.jpg"]) assert.equal(portalThumbnail("whatnot",url),null);
});
