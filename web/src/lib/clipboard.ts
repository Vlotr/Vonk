/**
 * copyText puts text on the clipboard. Without the clipboard API (plain
 * HTTP from another host than localhost) it copies through a selected text
 * field instead.
 */
export async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const field = document.createElement('textarea')
    field.value = text
    document.body.appendChild(field)
    field.select()
    document.execCommand('copy')
    field.remove()
  }
}
