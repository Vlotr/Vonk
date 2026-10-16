import type { ReactNode } from 'react'

// Line icons, drawn on a 24 by 24 grid with a 2 unit stroke in the current
// text color.

interface IconProps {
  className?: string
}

function Icon({ className, children }: IconProps & { children: ReactNode }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  )
}

/** GridIcon is four squares: All projects. */
export function GridIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="3" y="3" width="7" height="7" rx="1" />
      <rect x="14" y="3" width="7" height="7" rx="1" />
      <rect x="14" y="14" width="7" height="7" rx="1" />
      <rect x="3" y="14" width="7" height="7" rx="1" />
    </Icon>
  )
}

/** AlertCircleIcon is an exclamation mark in a circle: Issues. */
export function AlertCircleIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="12" cy="12" r="9" />
      <line x1="12" y1="8" x2="12" y2="12" />
      <line x1="12" y1="16" x2="12.01" y2="16" />
    </Icon>
  )
}

/** LinkIcon is two chain links: copy the link to a page. */
export function LinkIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
      <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
    </Icon>
  )
}

/** CheckIcon is a tick: the link was copied. */
export function CheckIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <polyline points="20 6 9 17 4 12" />
    </Icon>
  )
}

/** ChevronDownIcon is the thin arrow of a select, drawn 9 by 5. */
export function ChevronDownIcon({ className }: IconProps) {
  return (
    <svg className={className} viewBox="0 0 9 5" fill="none" stroke="currentColor" strokeWidth={1.2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <polyline points="0.6 0.6 4.5 4.4 8.4 0.6" />
    </svg>
  )
}

/** SettingsIcon is three sliders, each line broken where its knob sits: Settings. */
export function SettingsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h10M18 18h2" />
      <circle cx="15" cy="6" r="2" />
      <circle cx="9" cy="12" r="2" />
      <circle cx="16" cy="18" r="2" />
    </Icon>
  )
}

/** PlusIcon is a plus: Add project, Invite user. */
export function PlusIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <line x1="12" y1="4" x2="12" y2="20" />
      <line x1="4" y1="12" x2="20" y2="12" />
    </Icon>
  )
}

/** CopyIcon is two sheets: copy a value. */
export function CopyIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
    </Icon>
  )
}
