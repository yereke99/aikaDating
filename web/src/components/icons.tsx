/**
 * The call and moderation glyphs.
 *
 * The rest of the app draws its icons with typographic characters, which works for abstract marks
 * like ⌖ or ♥ but not for a call: the nearest available character reads as a media player's play
 * button, not as "start a video call". These are drawn instead, at the same visual weight, so the
 * meaning is unambiguous on both platforms and at any pixel density.
 */

function Icon({ path, label }: { path: string; label?: string }) {
  return (
    <svg
      className="icon"
      viewBox="0 0 24 24"
      width="1em"
      height="1em"
      fill="currentColor"
      focusable="false"
      aria-hidden={label ? undefined : true}
      role={label ? 'img' : undefined}
      aria-label={label}
    >
      <path d={path} />
    </svg>
  )
}

/** A camcorder: the standard "video call" mark. */
export const VideoCallIcon = (props: { label?: string }) => (
  <Icon {...props} path="M17 10.5V7c0-.55-.45-1-1-1H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.55 0 1-.45 1-1v-3.5l4 4v-11l-4 4z" />
)

/** A phone handset: the familiar compact call action. */
export const PhoneIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M6.62 10.79c1.44 2.83 3.76 5.14 6.59 6.59l2.2-2.2c.27-.27.67-.36 1.02-.24 1.12.37 2.33.57 3.57.57.55 0 1 .45 1 1V20c0 .55-.45 1-1 1C10.61 21 3 13.39 3 4c0-.55.45-1 1-1h3.5c.55 0 1 .45 1 1 0 1.24.2 2.45.57 3.57.11.35.03.74-.25 1.02l-2.2 2.2z"
  />
)

export const VideoOffIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M21 6.5l-4 4V7c0-.55-.45-1-1-1H9.82L21 17.18V6.5zM3.27 2L2 3.27 4.73 6H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.21 0 .39-.08.54-.18L19.73 21 21 19.73 3.27 2z"
  />
)

/** The handset every phone uses for "hang up" and "decline". */
export const CallEndIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M12 9c-1.6 0-3.15.25-4.6.72v3.1c0 .39-.23.74-.56.9-.98.49-1.87 1.12-2.66 1.85-.18.18-.43.28-.7.28-.28 0-.53-.11-.71-.29L.29 13.08c-.18-.17-.29-.42-.29-.7 0-.28.11-.53.29-.71C3.34 8.78 7.46 7 12 7s8.66 1.78 11.71 4.67c.18.18.29.43.29.71 0 .28-.11.53-.29.71l-2.48 2.48c-.18.18-.43.29-.71.29-.27 0-.52-.11-.7-.28-.79-.74-1.69-1.36-2.67-1.85-.33-.16-.56-.5-.56-.9v-3.1C15.15 9.25 13.6 9 12 9z"
  />
)

export const MicIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M12 14c1.66 0 3-1.34 3-3V5c0-1.66-1.34-3-3-3S9 3.34 9 5v6c0 1.66 1.34 3 3 3zm5.3-3c0 3-2.54 5.1-5.3 5.1S6.7 14 6.7 11H5c0 3.41 2.72 6.23 6 6.72V21h2v-3.28c3.28-.48 6-3.3 6-6.72h-1.7z"
  />
)

export const MicOffIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M19 11h-1.7c0 .74-.16 1.43-.43 2.05l1.23 1.23c.56-.98.9-2.09.9-3.28zm-4.02.17c0-.06.02-.11.02-.17V5c0-1.66-1.34-3-3-3S9 3.34 9 5v.18l5.98 5.99zM4.27 3L3 4.27l6.01 6.01V11c0 1.66 1.33 3 2.99 3 .22 0 .44-.03.65-.08l1.66 1.66c-.71.33-1.5.52-2.31.52-2.76 0-5.3-2.1-5.3-5.1H5c0 3.41 2.72 6.23 6 6.72V21h2v-3.28c.91-.13 1.77-.45 2.54-.9L19.73 21 21 19.73 4.27 3z"
  />
)

/** A camera with rotation arrows: switch between the front and back lens. */
export const FlipCameraIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M20 4h-3.17L15 2H9L7.17 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm-8 13c-2.76 0-5-2.24-5-5H5l2.5-2.5L10 12H8c0 2.21 1.79 4 4 4 .58 0 1.13-.13 1.62-.35l.74.74c-.71.38-1.51.61-2.36.61zm4.5-2.5L14 12h2c0-2.21-1.79-4-4-4-.58 0-1.13.13-1.62.35l-.74-.74C10.35 7.23 11.15 7 12 7c2.76 0 5 2.24 5 5h2l-2.5 2.5z"
  />
)

export const BlockIcon = (props: { label?: string }) => (
  <Icon
    {...props}
    path="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zM4 12c0-4.42 3.58-8 8-8 1.85 0 3.55.63 4.9 1.69L5.69 16.9C4.63 15.55 4 13.85 4 12zm8 8c-1.85 0-3.55-.63-4.9-1.69L18.31 7.1C19.37 8.45 20 10.15 20 12c0 4.42-3.58 8-8 8z"
  />
)
