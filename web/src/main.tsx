import '@fontsource-variable/instrument-sans/wght.css'
import '@fontsource-variable/jetbrains-mono/wght.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { App } from './App'
import { TOOL_NAME } from './brand'
import './styles/tokens.css'
import './styles/global.css'

document.title = TOOL_NAME

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
