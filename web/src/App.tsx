import { Route, Routes } from 'react-router'
import { AppShell } from './layout/AppShell'
import { IssueDetailPage } from './pages/IssueDetailPage'
import { IssuesPage } from './pages/IssuesPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { ProjectsPage } from './pages/ProjectsPage'

// App maps URLs to pages. Every page renders inside the AppShell frame.
export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<ProjectsPage />} />
        <Route path="projects/:id/issues" element={<IssuesPage />} />
        <Route path="projects/:id/issues/:issueId" element={<IssueDetailPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  )
}
