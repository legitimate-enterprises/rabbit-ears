import { browserEnv, snapshot, type Quality, type Snapshot } from "./capabilities";

/** The decoder group is probed once per page load; the rest is read fresh on every call. */
let decoderProbe: Promise<Snapshot["decoder"]> | undefined;

const VOLUME = "rabbit-ears.volume";
/** Starts el at the volume this device last chose and keeps whatever it is set to next. */
export function keepVolume(el: HTMLMediaElement) {
  try {
    const v = Number(localStorage.getItem(VOLUME) ?? "1");
    if (v >= 0 && v <= 1) el.volume = v;
  } catch {
    /* storage blocked: full volume */
  }
  el.addEventListener("volumechange", () => {
    try {
      localStorage.setItem(VOLUME, String(el.volume));
    } catch {
      /* storage blocked: the volume lasts until reload */
    }
  });
}

export interface Remember {
  profile: boolean;
  skipPin: boolean;
}
const rememberKey = (account: number) => `rabbit-ears.remember.${account}`;
/** This browser's Remember profile and Skip PIN for the adult signed in on it; both on until set. */
export function remembered(account: number): Remember {
  try {
    const v = JSON.parse(localStorage.getItem(rememberKey(account)) ?? "{}");
    return { profile: v.profile !== false, skipPin: v.skipPin !== false };
  } catch {
    return { profile: true, skipPin: true };
  }
}
export function remember(account: number, r: Remember) {
  try {
    localStorage.setItem(rememberKey(account), JSON.stringify(r));
  } catch {
    /* storage blocked: the defaults stand */
  }
}

/** Carries a lock or a pick to this browser's other tabs, which share its device. */
export const profileTabs = new BroadcastChannel("rabbit-ears.profile");

export async function hlsLibrarySupported(): Promise<boolean> {
  return (await import("hls.js")).default.isSupported();
}

export async function deviceSnapshot(quality?: Quality): Promise<Snapshot> {
  const env = browserEnv(await hlsLibrarySupported());
  const s = await snapshot(
    {
      ...env,
      decodingInfo: decoderProbe
        ? async () => ({ supported: false, powerEfficient: false, smooth: false })
        : env.decodingInfo,
    },
    quality,
  );
  if (decoderProbe) s.decoder = await decoderProbe;
  else decoderProbe = Promise.resolve(s.decoder);
  return s;
}
