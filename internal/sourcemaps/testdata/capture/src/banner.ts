// Code the second build has before the checkout, so its minified names
// and positions differ from the first build's.
export function greeting(name: string, hour: number) {
  const part = hour < 12 ? 'morning' : hour < 18 ? 'afternoon' : 'evening'
  return `Good ${part}, ${name}`
}

export function showBanner() {
  const banner = document.querySelector('#banner')
  if (banner) banner.textContent = greeting('visitor', new Date().getHours())
}
