// The channel decides which pipe, home and daemon a window uses. An installed
// copy carries no env, so the builder bakes the channel into the packaged
// manifest (extraMetadata.fltChannel); dev and smoke runs override through
// FLT_CHANNEL. Everything falls back to stable.

export function resolveChannel(
  envValue: string | undefined,
  packaged: boolean,
  manifestChannel: string | null,
): string {
  if (envValue) {
    return envValue;
  }
  if (packaged && (manifestChannel === "preview" || manifestChannel === "stable")) {
    return manifestChannel;
  }
  return "stable";
}
