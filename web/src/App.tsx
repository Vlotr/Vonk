import { Route, Routes } from 'react-router'
import { AppShell } from './layout/AppShell'
import { NotFoundPage } from './pages/NotFoundPage'
import { PlaceholderPage } from './pages/PlaceholderPage'

// App maps URLs to pages. Every page renders inside the AppShell frame.
export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<PlaceholderPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  )
}
