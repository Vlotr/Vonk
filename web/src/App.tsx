import { Navigate, Route, Routes } from 'react-router'
import { AppShell } from './layout/AppShell'
import { useMe } from './layout/MeContext'
import { AccountPage } from './pages/AccountPage'
import { AllProjectsPage } from './pages/AllProjectsPage'
import { ForgotPasswordPage } from './pages/ForgotPasswordPage'
import { InvitePage } from './pages/InvitePage'
import { IssueDetailPage } from './pages/IssueDetailPage'
import { IssuesPage } from './pages/IssuesPage'
import { LoginPage } from './pages/LoginPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { ProjectSetupPage } from './pages/ProjectSetupPage'
import { ResetPasswordPage } from './pages/ResetPasswordPage'
import { SettingsProjectsPage } from './pages/SettingsProjectsPage'
import { SettingsUsersPage } from './pages/SettingsUsersPage'
import { SetupPage } from './pages/SetupPage'

// App maps URLs to pages. The sign-in pages stand alone; every other page
// renders inside the AppShell frame and needs a session (the server sends
// visitors without one to /login).
export function App() {
  return (
    <Routes>
      <Route path="setup" element={<SetupPage />} />
      <Route path="login" element={<LoginPage />} />
      <Route path="invite" element={<InvitePage />} />
      <Route path="forgot-password" element={<ForgotPasswordPage />} />
      <Route path="reset-password" element={<ResetPasswordPage />} />
      <Route element={<AppShell />}>
        <Route index element={<AllProjectsPage />} />
        <Route path="projects/:id/issues" element={<IssuesPage />} />
        <Route path="projects/:id/issues/:issueId" element={<IssueDetailPage />} />
        <Route path="settings" element={<SettingsHome />} />
        <Route path="settings/projects" element={<SettingsProjectsPage />} />
        <Route path="settings/projects/:id/setup" element={<ProjectSetupPage />} />
        <Route path="settings/users" element={<SettingsUsersPage />} />
        <Route path="settings/account" element={<AccountPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  )
}

/** SettingsHome opens the first Settings page a user has: Projects for admins, Account for members. */
function SettingsHome() {
  const me = useMe()
  if (!me) return <section aria-busy="true" />
  return <Navigate to={me.role === 'admin' ? '/settings/projects' : '/settings/account'} replace />
}
